package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/handler"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/service"
)

var taskService *service.TaskService

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := gin.Default()
	// 不使用任意客户端传入的 X-Forwarded-For 作为可信身份。
	if err := r.SetTrustedProxies(nil); err != nil {
		log.Fatal(err)
	}
	r.Use(securityHeaders())

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})

	limits := newRequestLimits(
		envInteger("MAX_CONCURRENT_RESEARCH", 2, 1),
		envInteger("RATE_LIMIT_PER_MINUTE", 20, 0),
	)
	optionalAuth := func(c *gin.Context) {
		if c.GetHeader("Authorization") != "" {
			c.AbortWithStatusJSON(503, gin.H{"error": "未启用用户数据库，无法验证登录凭据"})
			return
		}
		c.Next()
	}
	if dsn := os.Getenv("MYSQL_DSN"); dsn != "" {
		startup, cancel := context.WithTimeout(ctx, 30*time.Second)
		store, err := repository.Open(startup, dsn)
		if err != nil {
			cancel()
			log.Fatal(err)
		}
		defer store.DB.Close()
		if err := store.Migrate(startup); err != nil {
			cancel()
			log.Fatalf("数据库迁移失败 error_type=%T", err)
		}
		cancel()
		cache, err := service.NewTaskCache(os.Getenv("REDIS_URL"), envOrDefault("CACHE_PREFIX", "arp"))
		if err != nil {
			log.Fatal(err)
		}
		defer cache.Close()
		taskService = &service.TaskService{Store: store, Cache: cache}
		accounts, err := handler.NewAccounts(taskService, os.Getenv("JWT_SECRET"))
		if err != nil {
			log.Fatal(err)
		}
		optionalAuth = accounts.Auth(false)
		authLimits := newRequestLimits(2, 20)
		r.POST("/api/auth/register", authLimits.middleware(), accounts.Register)
		r.POST("/api/auth/login", authLimits.middleware(), accounts.Login)
		tasks := r.Group("/api/tasks", accounts.Auth(true))
		tasks.GET("", accounts.ListTasks)
		tasks.GET("/:id", accounts.GetTask)
		tasks.GET("/:id/runs", accounts.GetRuns)
		tasks.GET("/:id/report", accounts.GetReport)
		log.Printf("[startup] mysql=ok redis=%s", cache.Health(ctx))
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				check, cancel := context.WithTimeout(ctx, 10*time.Second)
				taskService.RecoverExpired(check)
				cancel()
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	r.POST("/api/research", optionalAuth, limits.middleware(), handleResearch)
	r.GET("/health", func(c *gin.Context) {
		if taskService == nil {
			c.JSON(200, gin.H{"status": "ok", "mysql": "disabled", "redis": "disabled"})
			return
		}
		check, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		if taskService.Store.DB.PingContext(check) != nil {
			c.JSON(503, gin.H{"status": "unavailable", "mysql": "unavailable"})
			return
		}
		c.JSON(200, gin.H{"status": "ok", "mysql": "ok", "redis": taskService.Cache.Health(check)})
	})

	frontend, err := os.OpenRoot(envOrDefault("FRONTEND_DIR", "../frontend"))
	if err != nil {
		log.Printf("未加载前端目录（API 仍可使用）: %v", err)
	} else {
		defer frontend.Close()
		registerFrontend(r, frontend)
	}

	server := &http.Server{
		Addr:              envOrDefault("GO_ADDR", "127.0.0.1:8080"),
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      330 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	log.Printf("服务监听 %s；全局并发上限 %d，每分钟请求上限 %d（0 表示不限制频率）", server.Addr, cap(limits.slots), limits.perMinute)
	drained := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		close(drained)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-drained
}
