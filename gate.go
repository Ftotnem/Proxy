package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Ftotnem/GO-SERVICES/shared/config"
	redisu "github.com/Ftotnem/GO-SERVICES/shared/redis"
	"github.com/Ftotnem/GO-SERVICES/shared/registry" // Your registry package
	"go.minekube.com/gate/cmd/gate"
)

func main() {

	// 1. Load Configuration
	cfg, err := config.LoadCommonConfig() // Implement your LoadConfig
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// In a K8s pod, you'd get the pod's IP from the DOWNWARD API or environment variable
	// For local testing, you might hardcode or get from network interface
	// Example: Get IP from env (K8s: `valueFrom: { fieldRef: { fieldPath: status.podIP } }`)
	podIP := os.Getenv("POD_IP")
	if podIP == "" {
		podIP = "0.0.0.0" // Fallback for local dev
	}
	cfg.ServiceIP = podIP // Set the service IP in the config

	// Set the port this specific service exposes for others to connect to
	// For gate-proxy, this could be its HTTP API port (8080) for Minestom to register TO it
	// Or, if Minestom registers TO Redis, then this is just the main Minecraft listener port (25566)
	// For this example, let's assume it's the gate-proxy's public Minecraft port for simplicity in Redis.
	// We will clarify the Minestom-to-proxy registration below.
	cfg.ServicePort = 25566 // The port gate-proxy listens on for Minecraft clients

	// 2. Initialize Redis Client
	// For Kubernetes, cfg.Redis.Addresses should be `redis-cluster-service:6379`
	rdb, err := redisu.NewRedisClusterClient(cfg.RedisAddrs)
	if err != nil {
		log.Fatalf("Could not connect to Redis: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Could not connect to Redis: %v", err)
	}
	log.Println("Successfully connected to Redis!")

	// 3. Create and Start ServiceRegistrar
	// The serviceType for gate-proxy is "proxy"
	registrar := registry.NewServiceRegistrar(rdb, "proxy", &cfg)
	registrar.Start() // This will start heartbeating to Redis

	// 4. Start your actual Gate-Proxy application logic
	// ... your Minecraft proxy server (listening on 25566)
	// ... your HTTP API server (listening on 8080 for Minestom registration)

	gate.Execute()
	log.Println("Gate-Proxy service started (waiting for players or registration requests from Minestom)...")

	// 5. Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down Gate-Proxy...")
	registrar.Stop() // This will stop heartbeating and deregister from Redis

	// ... additional cleanup for your Gate-Proxy application
	log.Println("Gate-Proxy gracefully stopped.")
}
