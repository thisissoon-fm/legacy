package main

import (
	"fmt"

	"legacy/config"
	"legacy/legacy"
	"legacy/logger"
	"legacy/pubsub/redis"
	"legacy/run"
)

func main() {
	if err := config.Load(""); err != nil {
		fmt.Println(fmt.Sprintf("error loading config: %s", err))
		return
	}
	logger.Setup()
	rconfig := redis.NewConfig()
	logger.WithFields(logger.F{
		"host":   rconfig.Host(),
		"topics": rconfig.Topics(),
	}).Debug("redis configuration")
	rc := redis.New(rconfig)
	legacy.AddPubSub("redis", rc)
	defer legacy.Close()
	if err := legacy.Start(legacy.NewConfig()); err != nil {
		fmt.Println("Error:", err)
		return
	}
	<-run.UntilQuit()
	fmt.Println("exit")
}
