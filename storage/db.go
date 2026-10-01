package storage

import (
	"fmt"

	"MajSpirit/config"
	"MajSpirit/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func InitDB(cfg *config.Config) error {
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable TimeZone=Asia/Shanghai", cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPassword, cfg.DBName)
	var err error

	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})

	if err != nil {
		return fmt.Errorf("连接失败:%w", err)
	}

	err = DB.AutoMigrate(
		&model.User{},
		&model.Game{},
		&model.Round{},
	)

	if err != nil {
		return fmt.Errorf("迁移失败:%w", err)
	}

	fmt.Println("连接成功")
	return nil
}
