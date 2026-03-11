package main

import (
	"flag"
	"upay_pro/cron"
	"upay_pro/db/sdb"
	"upay_pro/mylog"
	"upay_pro/web"

	"go.uber.org/zap"
)

func main() {
	currentPort := sdb.GetSetting().Httpport
	httpPort := flag.Int("p", currentPort, "HTTP listen port")
	flag.Parse()

	if *httpPort < 1 || *httpPort > 65535 {
		mylog.Logger.Warn("HTTP端口无效，使用默认端口", zap.Int("port", *httpPort), zap.Int("default_port", sdb.DefaultHTTPPort))
		*httpPort = sdb.DefaultHTTPPort
	}

	flagProvided := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "p" {
			flagProvided = true
		}
	})

	if flagProvided && *httpPort != currentPort {
		if err := sdb.UpdateHttpPort(*httpPort); err != nil {
			mylog.Logger.Warn("更新HTTP端口到数据库失败，继续使用数据库中的端口", zap.Error(err))
		}
	}

	defer func() {
		if err := recover(); err != nil {
			mylog.Logger.Error("程序发生恐慌导致崩溃", zap.Any("error", err))
		}

	}()

	{
		go cron.Start()
		web.Start()

	}

}
