package redis

import (
	"fmt"

	"github.com/go-redis/redis/v8"
)

type Redis struct {
	client *redis.Client
}

type Options struct {
	Host     string
	Port     int
	Username string
	Password string
	Options  string
}

func NewRedis(options Options) (*Redis, error) {
	var client *redis.Client

	if len(options.Host) > 0 {
		redisOptions, err := redis.ParseURL(fmt.Sprintf("redis://%s:%d?%s", options.Host, options.Port, options.Options))
		if err != nil {
			return nil, fmt.Errorf("invalid redis options: %v", options)
		}

		redisOptions.Username = options.Username
		redisOptions.Password = options.Password

		client = redis.NewClient(redisOptions)
	}

	return &Redis{client: client}, nil
}
