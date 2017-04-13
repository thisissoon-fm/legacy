// Legacy Configuration

package legacy

import "github.com/spf13/viper"

const (
	viper_legacy_redis_host_key = "legacy.redis.host"
)

func init() {
	viper.SetDefault(viper_legacy_redis_host_key, "localhost:6379")
	viper.BindEnv(viper_legacy_redis_host_key)
}

type Configurer interface {
	LegacyRedisHost() string
}

type Config struct{}

func (c Config) LegacyRedisHost() string {
	return viper.GetString(viper_legacy_redis_host_key)
}

func NewConfig() Config {
	return Config{}
}
