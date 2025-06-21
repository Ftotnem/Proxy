package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Ftotnem/GO-SERVICES/shared/config"
	redisu "github.com/Ftotnem/GO-SERVICES/shared/redis"
	"github.com/Ftotnem/GO-SERVICES/shared/registry"
	"go.minekube.com/gate/cmd/gate"
)

// RedisConfig holds retry configuration
type RedisConfig struct {
	MaxRetries   int
	BaseDelay    time.Duration
	MaxDelay     time.Duration
	HealthCheck  time.Duration
	StartupGrace time.Duration // How long to wait for Redis on startup before giving up
}

// ProxyService manages the proxy lifecycle
type ProxyService struct {
	cfg         *config.CommonConfig
	registrar   *registry.ServiceRegistrar
	redisConfig RedisConfig
	mu          sync.RWMutex
	shutdown    chan struct{}
}

func main() {
	// 1. Load Configuration
	cfg, err := config.LoadCommonConfig()
	if err != nil {
		log.Printf("ERROR: Failed to load config: %v", err)
		return
	}
	cfg.RedisAddrs = []string{"0.0.0.0:7000", "0.0.0.0:7001", "0.0.0.0:7002", "0.0.0.0:7003", "0.0.0.0:7004", "0.0.0.0:7005"} // Default for K8s Service

	// Set up pod IP and service port
	podIP := os.Getenv("POD_IP")
	if podIP == "" {
		podIP = "0.0.0.0"
	}
	cfg.ServiceIP = podIP
	cfg.ServicePort = 25565

	// 2. Create proxy service with configuration
	proxy := &ProxyService{
		cfg: &cfg,
		redisConfig: RedisConfig{
			MaxRetries:   15, // More retries for k8s environments
			BaseDelay:    2 * time.Second,
			MaxDelay:     30 * time.Second,
			HealthCheck:  30 * time.Second,
			StartupGrace: 2 * time.Minute, // Wait up to 2 minutes for Redis on startup
		},
		shutdown: make(chan struct{}),
	}

	// 3. Start the proxy service
	if err := proxy.Start(); err != nil {
		log.Printf("ERROR: Failed to start proxy service: %v", err)
		return
	}

	// 4. Handle graceful shutdown
	proxy.WaitForShutdown()
}

func (p *ProxyService) Start() error {
	log.Println("INFO: Starting Gate-Proxy service...")

	// Start Gate-Proxy immediately (don't wait for Redis)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("ERROR: Gate-Proxy crashed: %v", r)
			}
		}()
		gate.Execute()
	}()

	log.Println("INFO: Gate-Proxy started and accepting connections")

	// Try to connect to Redis with a startup grace period
	go p.initializeRedisWithGrace()

	return nil
}

func (p *ProxyService) initializeRedisWithGrace() {
	log.Println("INFO: Attempting initial Redis connection...")

	// Try aggressively for the first few minutes
	startupCtx, cancel := context.WithTimeout(context.Background(), p.redisConfig.StartupGrace)
	defer cancel()

	attempt := 0
	for {
		select {
		case <-startupCtx.Done():
			log.Println("WARN: Redis not available during startup grace period. Proxy will continue without service registration.")
			log.Println("WARN: Will continue trying to connect to Redis in background...")
			go p.backgroundRedisRetry()
			return
		default:
			attempt++
			if p.tryConnectRedis(attempt, true) {
				return // Successfully connected
			}
			// Wait before next attempt
			p.waitBeforeRetry(attempt, true)
		}
	}
}

func (p *ProxyService) backgroundRedisRetry() {
	attempt := 0
	for {
		select {
		case <-p.shutdown:
			return
		default:
			attempt++
			if p.tryConnectRedis(attempt, false) {
				// Start monitoring the connection
				go p.monitorRedisConnection()
				return
			}
			p.waitBeforeRetry(attempt, false)
		}
	}
}

func (p *ProxyService) tryConnectRedis(attempt int, isStartup bool) bool {
	logLevel := "INFO"
	if !isStartup {
		logLevel = "DEBUG"
	}

	log.Printf("%s: Redis connection attempt %d...", logLevel, attempt)

	// Try to connect
	rdb, err := redisu.NewRedisClusterClient(p.cfg.RedisAddrs, p.cfg.RedisPassword)
	if err != nil {
		log.Printf("WARN: Redis connection failed (attempt %d): %v", attempt, err)
		return false
	}

	// Test the connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Printf("WARN: Redis ping failed (attempt %d): %v", attempt, err)
		return false
	}

	// Success! Start the registrar
	p.mu.Lock()
	defer p.mu.Unlock()

	log.Println("INFO: Redis connection successful!")
	p.registrar = registry.NewServiceRegistrar(rdb, "proxy", p.cfg)
	p.registrar.Start()
	log.Println("INFO: Service registration started")

	return true
}

func (p *ProxyService) waitBeforeRetry(attempt int, isStartup bool) {
	baseDelay := p.redisConfig.BaseDelay
	maxDelay := p.redisConfig.MaxDelay

	// Faster retries during startup
	if isStartup {
		baseDelay = 1 * time.Second
		maxDelay = 10 * time.Second
	}

	// Exponential backoff
	delay := baseDelay * time.Duration(1<<uint(min(attempt-1, 6))) // Cap at 2^6
	if delay > maxDelay {
		delay = maxDelay
	}

	select {
	case <-p.shutdown:
		return
	case <-time.After(delay):
		return
	}
}

func (p *ProxyService) monitorRedisConnection() {
	ticker := time.NewTicker(p.redisConfig.HealthCheck)
	defer ticker.Stop()

	for {
		select {
		case <-p.shutdown:
			return
		case <-ticker.C:
			p.mu.RLock()
			hasRegistrar := p.registrar != nil
			p.mu.RUnlock()

			if !hasRegistrar {
				log.Println("WARN: Redis registrar lost, attempting to reconnect...")
				go p.backgroundRedisRetry()
				return
			}

			// Optionally add health check for the registrar here
			// if p.registrar.IsHealthy() == false { ... }
		}
	}
}

func (p *ProxyService) WaitForShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("INFO: Shutdown signal received...")
	close(p.shutdown)

	p.mu.RLock()
	registrar := p.registrar
	p.mu.RUnlock()

	if registrar != nil {
		log.Println("INFO: Stopping service registration...")
		registrar.Stop()
	}

	log.Println("INFO: Gate-Proxy gracefully stopped")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
