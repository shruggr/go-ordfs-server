package cache

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
	"github.com/shruggr/go-ordfs-server/internal/config"
)

type RedisCache struct {
	client *redis.Client
}

func NewRedisCache(cfg *config.Config) (*RedisCache, error) {
	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis URL: %w", err)
	}

	opts.MaintNotificationsConfig = &maintnotifications.Config{
		Mode: maintnotifications.ModeDisabled,
	}

	client := redis.NewClient(opts)

	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &RedisCache{
		client: client,
	}, nil
}

func (r *RedisCache) Client() *redis.Client {
	return r.client
}

func (r *RedisCache) Close() error {
	return r.client.Close()
}
