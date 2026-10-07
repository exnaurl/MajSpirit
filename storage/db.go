package storage

import (
	"fmt"
	"log"
	"time"

	"MajSpirit/config"
	"MajSpirit/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func InitDB(cfg *config.Config) error {
	// connect_timeout：数据库连不上时最多等 5 秒就报错，不要一直卡着（否则表现为"没输出也没报错"）
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s connect_timeout=5 TimeZone=%s",
		cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPassword, cfg.DBName, cfg.DBSSLMode, cfg.DBTimeZone)

	log.Printf("正在连接数据库 %s:%s/%s（用户 %s）...", cfg.DBHost, cfg.DBPort, cfg.DBName, cfg.DBUser)

	var err error

	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		// 生产别刷 SQL 日志：又吵又容易把用户名/密码哈希写进日志
		Logger: logger.Default.LogMode(logger.Warn),
	})

	if err != nil {
		return fmt.Errorf("连接数据库失败（%s:%s/%s）：%w", cfg.DBHost, cfg.DBPort, cfg.DBName, err)
	}

	// 连接池：4 人一局每次操作都会写一次库，池子开着免得高峰排队
	if sqlDB, err := DB.DB(); err == nil {
		sqlDB.SetMaxOpenConns(20)
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetConnMaxLifetime(time.Hour)
		sqlDB.SetConnMaxIdleTime(10 * time.Minute)
	}

	log.Println("数据库已连接，开始自动迁移...")

	err = DB.AutoMigrate(
		&model.User{},
		&model.Game{},
	)

	if err != nil {
		return fmt.Errorf("自动迁移失败：%w", err)
	}

	log.Println("数据库就绪")
	return nil
}
