package handler

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/service"
	"golang.org/x/crypto/bcrypt"
)

const userKey = "authenticated_user_id"
const issuer = "ai-research-platform"

type Accounts struct {
	Tasks     *service.TaskService
	secret    []byte
	dummyHash []byte
}

func NewAccounts(tasks *service.TaskService, secret string) (*Accounts, error) {
	if len(secret) < 32 {
		return nil, errors.New("启用 MySQL 时 JWT_SECRET 至少需要 32 字节")
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte(repository.NewID()), bcrypt.DefaultCost)
	return &Accounts{Tasks: tasks, secret: []byte(secret), dummyHash: dummy}, err
}

func UserID(c *gin.Context) string { return c.GetString(userKey) }

func (a *Accounts) Auth(required bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" && !required {
			c.Next()
			return
		}
		parts := strings.Fields(header)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(401, gin.H{"error": "请提供有效的登录凭据"})
			return
		}
		claims := &jwt.RegisteredClaims{}
		token, err := jwt.ParseWithClaims(parts[1], claims, func(t *jwt.Token) (any, error) { return a.secret, nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(issuer), jwt.WithAudience(issuer), jwt.WithExpirationRequired())
		if err != nil || !token.Valid || len(claims.Subject) != 32 {
			c.AbortWithStatusJSON(401, gin.H{"error": "登录凭据无效或已过期"})
			return
		}
		c.Set(userKey, claims.Subject)
		c.Set(authExpiryKey, claims.ExpiresAt.Time)
		c.Next()
	}
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func readCredentials(c *gin.Context) (credentials, bool) {
	var input credentials
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(400, gin.H{"error": "请提交 email 和 password"})
		return input, false
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	address, err := mail.ParseAddress(input.Email)
	if err != nil || address.Address != input.Email || len(input.Email) > 254 || len(input.Password) < 8 || len(input.Password) > 72 {
		c.JSON(400, gin.H{"error": "邮箱格式无效，或密码不在 8～72 字节范围内"})
		return input, false
	}
	return input, true
}
func (a *Accounts) Register(c *gin.Context) {
	input, ok := readCredentials(c)
	if !ok {
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		serverError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	u, err := a.Tasks.Store.CreateUser(ctx, input.Email, string(hash))
	if errors.Is(err, repository.ErrConflict) {
		c.JSON(409, gin.H{"error": "该邮箱已注册"})
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(201, gin.H{"user": u})
}
func (a *Accounts) Login(c *gin.Context) {
	input, ok := readCredentials(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	u, err := a.Tasks.Store.UserByEmail(ctx, input.Email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		serverError(c, err)
		return
	}
	hash := []byte(u.PasswordHash)
	if err != nil {
		hash = a.dummyHash
	}
	passwordErr := bcrypt.CompareHashAndPassword(hash, []byte(input.Password))
	if err != nil || passwordErr != nil {
		c.JSON(401, gin.H{"error": "邮箱或密码错误"})
		return
	}
	now := time.Now()
	expiry := now.Add(24 * time.Hour)
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Issuer: issuer, Subject: u.ID, Audience: jwt.ClaimStrings{issuer}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expiry)}).SignedString(a.secret)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(200, gin.H{"access_token": token, "token_type": "Bearer", "expires_in": 86400, "user": u})
}

func serverError(c *gin.Context, err error) {
	log.Printf("[api] database operation failed error_type=%T", err)
	c.JSON(503, gin.H{"error": "数据服务暂时不可用，请稍后重试"})
}
func (a *Accounts) ListTasks(c *gin.Context) {
	page, err1 := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, err2 := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if err1 != nil || err2 != nil || page < 1 || page > 1000000 || size < 1 || size > 100 {
		c.JSON(400, gin.H{"error": "page 应为 1～1000000，page_size 应为 1～100"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	items, total, err := a.Tasks.Store.Tasks(ctx, UserID(c), page, size)
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
func taskError(c *gin.Context, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(404, gin.H{"error": "任务或报告不存在"})
	} else {
		serverError(c, err)
	}
}
func (a *Accounts) GetTask(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	t, err := a.Tasks.Get(ctx, UserID(c), c.Param("id"))
	if err != nil {
		taskError(c, err)
		return
	}
	c.JSON(200, t)
}
func (a *Accounts) GetRuns(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	// 直接查数据库确认归属，避免以空列表泄漏任务是否存在。
	if _, err := a.Tasks.Store.Task(ctx, UserID(c), c.Param("id")); err != nil {
		taskError(c, err)
		return
	}
	runs, err := a.Tasks.Store.Runs(ctx, UserID(c), c.Param("id"))
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": runs})
}
func (a *Accounts) GetReport(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	r, err := a.Tasks.Store.Report(ctx, UserID(c), c.Param("id"))
	if err != nil {
		taskError(c, err)
		return
	}
	c.JSON(200, r)
}
