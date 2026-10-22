package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/model"
)

//go:embed migrations/*.sql
var migrations embed.FS

var ErrConflict = errors.New("record conflicts with existing state")

type Store struct{ DB *sql.DB }

func NewID() string { return hex.EncodeToString(randomBytes()) }
func randomBytes() []byte {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	return open(ctx, dsn, false)
}

// OpenWithRetry is for process startup. Health probes use Open to fail promptly.
// The caller must provide a deadline so a missing database cannot hang startup.
func OpenWithRetry(ctx context.Context, dsn string) (*Store, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		return nil, errors.New("MySQL 启动重试需要 context 截止时间")
	}
	return open(ctx, dsn, true)
}

func open(ctx context.Context, dsn string, retry bool) (*Store, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("MYSQL_DSN 格式无效")
	}
	cfg.ParseTime, cfg.Loc = true, time.UTC
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 3*time.Second, 5*time.Second, 5*time.Second
	cfg.MultiStatements = false
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, errors.New("无法创建 MySQL 客户端")
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(3 * time.Minute)
	if retry {
		err = pingWithRetry(ctx, db.PingContext, 250*time.Millisecond)
	} else {
		err = db.PingContext(ctx)
	}
	if err != nil {
		db.Close()
		// Driver errors can contain addresses/usernames. Never return the DSN.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("MySQL 连接等待结束: %w", ctx.Err())
		}
		return nil, errors.New("MySQL 连接检查失败，请检查地址、数据库及账号配置")
	}
	return &Store{DB: db}, nil
}

// MySQL DDL 隐式提交；迁移使用可重入 DDL，全部成功后才写入版本及校验和。
// 命名锁固定在同一连接上，防止两个进程同时迁移。
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var locked int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(CONCAT(DATABASE(), ':migrate'), 10)").Scan(&locked); err != nil {
		return err
	}
	if locked != 1 {
		return errors.New("migration lock unavailable")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(cleanup, "DO RELEASE_LOCK(CONCAT(DATABASE(), ':migrate'))")
	}()
	_, err = conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
	 version VARCHAR(100) PRIMARY KEY, checksum CHAR(64) NOT NULL, applied_at DATETIME(6) NOT NULL)`)
	if err != nil {
		return err
	}
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	for _, file := range files {
		body, err := migrations.ReadFile("migrations/" + file.Name())
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		checksum := hex.EncodeToString(sum[:])
		var existing string
		err = conn.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE version=?", file.Name()).Scan(&existing)
		if err == nil {
			if existing != checksum {
				return fmt.Errorf("migration %s checksum changed", file.Name())
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		for _, statement := range strings.Split(string(body), ";") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if _, err = conn.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		if _, err = conn.ExecContext(ctx, "INSERT INTO schema_migrations VALUES (?, ?, ?)", file.Name(), checksum, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateUser(ctx context.Context, email, hash string) (model.User, error) {
	u := model.User{ID: NewID(), Email: email, PasswordHash: hash, CreatedAt: time.Now().UTC()}
	_, err := s.DB.ExecContext(ctx, "INSERT INTO users (id,email,password_hash,created_at) VALUES (?,?,?,?)", u.ID, u.Email, u.PasswordHash, u.CreatedAt)
	var duplicate *mysql.MySQLError
	if errors.As(err, &duplicate) && duplicate.Number == 1062 {
		err = ErrConflict
	}
	return u, err
}

func (s *Store) UserByEmail(ctx context.Context, email string) (model.User, error) {
	var u model.User
	err := s.DB.QueryRowContext(ctx, "SELECT id,email,password_hash,created_at FROM users WHERE email=?", email).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt)
	return u, err
}

const taskColumns = "id,user_id,query,mode,status,started_at,finished_at,deadline_at,error_code,error_message,version"

type scanner interface{ Scan(...any) error }

func scanTask(row scanner) (model.Task, error) {
	var t model.Task
	err := row.Scan(&t.ID, &t.UserID, &t.Query, &t.Mode, &t.Status, &t.StartedAt, &t.FinishedAt, &t.DeadlineAt, &t.ErrorCode, &t.ErrorMessage, &t.Version)
	return t, err
}

func (s *Store) CreateTask(ctx context.Context, userID, query, mode string, opts ...TaskOptions) (model.Task, error) {
	now := time.Now().UTC()
	budget := 150 * time.Second
	if mode == "research" {
		budget = 330 * time.Second
	}
	t := model.Task{ID: NewID(), UserID: userID, Query: query, Mode: mode, Status: "running", StartedAt: now, DeadlineAt: now.Add(budget), Version: 1}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return t, err
	}
	defer tx.Rollback()
	var locked string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE id=? FOR UPDATE", userID).Scan(&locked); err != nil {
		return t, err
	}
	if err = checkDaily(ctx, tx, userID, options(opts).DailyLimit); err != nil {
		return t, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks (id,user_id,query,mode,status,started_at,deadline_at,error_message) VALUES (?,?,?,?,?,?,?,'')`, t.ID, t.UserID, t.Query, t.Mode, t.Status, t.StartedAt, t.DeadlineAt)
	if err != nil {
		return t, err
	}
	if err = saveObservation(ctx, tx, t.ID, options(opts).RequestID); err != nil {
		return t, err
	}
	return t, tx.Commit()
}

func (s *Store) Task(ctx context.Context, userID, id string) (model.Task, error) {
	return scanTaskView(s.DB.QueryRowContext(ctx, "SELECT "+taskViewColumns+" FROM tasks t LEFT JOIN task_jobs j ON j.task_id=t.id WHERE t.user_id=? AND t.id=?", userID, id))
}

func (s *Store) Tasks(ctx context.Context, userID string, page, size int) ([]model.Task, int, error) {
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE user_id=?", userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT "+taskViewColumns+" FROM tasks t LEFT JOIN task_jobs j ON j.task_id=t.id WHERE t.user_id=? ORDER BY t.started_at DESC,t.id DESC LIMIT ? OFFSET ?", userID, size, (page-1)*size)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]model.Task, 0)
	for rows.Next() {
		t, err := scanTaskView(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, t)
	}
	return items, total, rows.Err()
}

// 状态、阶段记录和报告在同一事务中提交；失败时全部回滚。
func (s *Store) Finish(ctx context.Context, userID, id string, report *model.Report, runs []model.AgentRun, code, message string, usage ...*model.Usage) (model.Task, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Task{}, err
	}
	defer tx.Rollback()
	t, err := scanTask(tx.QueryRowContext(ctx, "SELECT "+taskColumns+" FROM tasks WHERE id=? AND user_id=? FOR UPDATE", id, userID))
	if err != nil {
		return t, err
	}
	if t.Status != "running" {
		return t, ErrConflict
	}
	var measured *model.Usage
	if len(usage) > 0 {
		measured = usage[0]
	}
	if err = saveUsage(ctx, tx, id, 0, measured); err != nil {
		return t, err
	}
	t, err = saveTaskResult(ctx, tx, t, report, runs, code, message)
	if err != nil {
		return t, err
	}
	if err = appendEventLocked(ctx, tx, t, nil); err != nil {
		return t, err
	}
	return t, tx.Commit()
}

func saveTaskResult(ctx context.Context, tx *sql.Tx, t model.Task, report *model.Report, runs []model.AgentRun, code, message string) (model.Task, error) {
	var err error
	now := time.Now().UTC()
	t.FinishedAt = &now
	t.Version++
	t.Status = "failed"
	t.ErrorCode = code
	t.ErrorMessage = message
	if report != nil {
		if strings.TrimSpace(report.Result) == "" {
			return t, errors.New("empty report")
		}
		t.Status = "succeeded"
		t.ErrorCode = ""
		t.ErrorMessage = ""
		var plan, search any
		if len(report.Plan) > 0 {
			plan = string(report.Plan)
		}
		if len(report.Search) > 0 {
			search = string(report.Search)
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO reports (task_id,result,plan,search) VALUES (?,?,?,?)", t.ID, report.Result, plan, search)
		if err != nil {
			return t, err
		}
	}
	if len(runs) == 0 {
		runs = []model.AgentRun{{Stage: t.Mode, Status: t.Status, StartedAt: t.StartedAt, FinishedAt: now, ErrorCode: t.ErrorCode}}
	}
	for i, run := range runs {
		_, err = tx.ExecContext(ctx, "INSERT INTO agent_runs (task_id,sequence_no,stage,status,started_at,finished_at,error_code) VALUES (?,?,?,?,?,?,?)", t.ID, i, run.Stage, run.Status, run.StartedAt, run.FinishedAt, run.ErrorCode)
		if err != nil {
			return t, err
		}
	}
	_, err = tx.ExecContext(ctx, "UPDATE tasks SET status=?,finished_at=?,error_code=?,error_message=?,version=? WHERE id=?", t.Status, now, t.ErrorCode, t.ErrorMessage, t.Version, t.ID)
	if err != nil {
		return t, err
	}
	return t, nil
}

func (s *Store) Runs(ctx context.Context, userID, id string) ([]model.AgentRun, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT a.stage,a.status,a.started_at,a.finished_at,a.error_code FROM agent_runs a JOIN tasks t ON t.id=a.task_id WHERE t.user_id=? AND t.id=? ORDER BY a.sequence_no`, userID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]model.AgentRun, 0)
	for rows.Next() {
		var r model.AgentRun
		if err := rows.Scan(&r.Stage, &r.Status, &r.StartedAt, &r.FinishedAt, &r.ErrorCode); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (s *Store) Report(ctx context.Context, userID, id string) (model.Report, error) {
	var r model.Report
	var plan, search []byte
	err := s.DB.QueryRowContext(ctx, `SELECT r.result,t.mode,r.plan,r.search FROM reports r JOIN tasks t ON t.id=r.task_id WHERE t.user_id=? AND t.id=? AND t.status='succeeded'`, userID, id).Scan(&r.Result, &r.Mode, &plan, &search)
	r.Plan = plan
	r.Search = search
	return r, err
}

func (s *Store) Expired(ctx context.Context) ([]model.Task, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+taskColumns+" FROM tasks WHERE status='running' AND deadline_at<? AND NOT EXISTS (SELECT 1 FROM task_jobs j WHERE j.task_id=tasks.id) LIMIT 100", time.Now().UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Task, 0)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, t)
	}
	return items, rows.Err()
}
