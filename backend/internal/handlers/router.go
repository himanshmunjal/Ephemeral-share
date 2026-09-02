package handlers

import (
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"ephemeral-share/internal/analytics"
	"ephemeral-share/internal/config"
	"ephemeral-share/internal/middleware"
	"ephemeral-share/internal/redisclient"
	"ephemeral-share/internal/storage"
)

func NewRouter(store *redisclient.Store, s3 *storage.Client, cfg *config.Config, stats *analytics.Store) *gin.Engine {
	r := gin.Default()

	r.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.AllowedOrigins,
		AllowMethods:     []string{"GET", "POST", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept"},
		AllowCredentials: true,
	}))

	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })

	sessionH := NewSessionHandler(store, s3, cfg, stats)
	contentH := NewContentHandler(store, s3, cfg, stats)
	wsH := NewWebSocketHandler(store, cfg.AllowedOrigins)

	api := r.Group("/api/v1")
	{
		api.POST("/sessions", middleware.RateLimit(cfg.RateLimitPerMin), sessionH.CreateSession)
		api.GET("/sessions/:code", middleware.RateLimit(cfg.RateLimitPerMin*2), sessionH.GetSession)
		api.POST("/sessions/:code/join", middleware.RateLimit(cfg.RateLimitPerMin*2), sessionH.JoinSession)
		api.DELETE("/sessions/:code", sessionH.DeleteSession)

		api.POST("/sessions/:code/content/upload-url", contentH.RequestUploadURL)
		api.POST("/sessions/:code/content/text", contentH.AddTextContent)
		api.GET("/sessions/:code/content", contentH.ListContent)
		api.GET("/sessions/:code/content/:id/download", contentH.DownloadContent)
	}

	r.GET("/ws/sessions/:code", wsH.HandleConnection)

	return r
}
