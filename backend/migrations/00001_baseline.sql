-- goose 基线迁移：= Alembic head e3f4a5b6c7d7 全量 DDL（11 张业务表）
-- 生成方式：空库 alembic upgrade head → mysqldump --no-data → 剔除 alembic_version/AUTO_INCREMENT 计数
-- 启动引导三态见 docs/v2/04-DATA-MODEL.md §4；过渡期零 schema 变更，增量从 00002 起

-- +goose Up
-- +goose StatementBegin
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `contact_groups` (
  `id` int NOT NULL AUTO_INCREMENT,
  `name` varchar(255) DEFAULT NULL,
  `description` varchar(500) DEFAULT NULL,
  `user_id` int DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `user_id` (`user_id`),
  KEY `ix_contact_groups_id` (`id`),
  KEY `ix_contact_groups_name` (`name`),
  CONSTRAINT `contact_groups_ibfk_1` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `contacts` (
  `id` int NOT NULL AUTO_INCREMENT,
  `email` varchar(255) DEFAULT NULL,
  `name` varchar(255) DEFAULT NULL,
  `group_id` int DEFAULT NULL,
  `attributes` text,
  PRIMARY KEY (`id`),
  KEY `group_id` (`group_id`),
  KEY `ix_contacts_email` (`email`),
  KEY `ix_contacts_id` (`id`),
  CONSTRAINT `contacts_ibfk_1` FOREIGN KEY (`group_id`) REFERENCES `contact_groups` (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `email_blacklist` (
  `id` int NOT NULL AUTO_INCREMENT,
  `email` varchar(255) NOT NULL,
  `reason` varchar(500) DEFAULT '',
  `created_by` varchar(100) DEFAULT 'admin',
  `created_at` datetime DEFAULT (now()),
  PRIMARY KEY (`id`),
  UNIQUE KEY `ix_email_blacklist_email` (`email`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `email_templates` (
  `id` int NOT NULL AUTO_INCREMENT,
  `name` varchar(255) DEFAULT NULL,
  `ses_name` varchar(255) DEFAULT NULL,
  `subject` varchar(500) DEFAULT NULL,
  `html_body` text,
  `text_body` text,
  `user_id` int DEFAULT NULL,
  `created_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `ix_email_templates_ses_name` (`ses_name`),
  KEY `user_id` (`user_id`),
  KEY `ix_email_templates_id` (`id`),
  KEY `ix_email_templates_name` (`name`),
  CONSTRAINT `email_templates_ibfk_1` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `scheduled_jobs` (
  `id` int NOT NULL AUTO_INCREMENT,
  `user_id` int NOT NULL,
  `template_id` int NOT NULL,
  `group_id` int NOT NULL,
  `template_name` varchar(255) DEFAULT NULL,
  `group_name` varchar(255) DEFAULT NULL,
  `schedule_type` varchar(16) NOT NULL,
  `scheduled_time` datetime NOT NULL,
  `cron_hour` int DEFAULT '9',
  `cron_minute` int DEFAULT '0',
  `day_of_week` int DEFAULT NULL,
  `day_of_month` int DEFAULT NULL,
  `status` varchar(16) DEFAULT 'active',
  `next_run_at` datetime DEFAULT NULL,
  `last_run_at` datetime DEFAULT NULL,
  `run_count` int DEFAULT '0',
  `last_batch_id` varchar(64) DEFAULT NULL,
  `error_message` text,
  `created_at` datetime DEFAULT (now()),
  `updated_at` datetime DEFAULT (now()),
  PRIMARY KEY (`id`),
  KEY `ix_scheduled_jobs_next_run_at` (`next_run_at`),
  KEY `ix_scheduled_jobs_user_id` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `sending_job_details` (
  `id` int NOT NULL AUTO_INCREMENT,
  `job_id` int DEFAULT NULL,
  `batch_id` varchar(64) DEFAULT NULL,
  `message_id` varchar(128) DEFAULT NULL,
  `recipient` varchar(256) DEFAULT NULL,
  `send_status` varchar(32) DEFAULT 'Pending',
  `send_error` text,
  `delivery_status` varchar(32) DEFAULT NULL,
  `delivery_time` datetime DEFAULT NULL,
  `bounce_type` varchar(64) DEFAULT NULL,
  `bounce_subtype` varchar(64) DEFAULT NULL,
  `bounce_message` text,
  `open_count` int DEFAULT '0',
  `first_open_time` datetime DEFAULT NULL,
  `click_count` int DEFAULT '0',
  `first_click_time` datetime DEFAULT NULL,
  `complaint_time` datetime DEFAULT NULL,
  `created_at` datetime DEFAULT (now()),
  PRIMARY KEY (`id`),
  KEY `ix_sending_job_details_batch_id` (`batch_id`),
  KEY `ix_sending_job_details_recipient` (`recipient`),
  KEY `ix_sending_job_details_job_id` (`job_id`),
  KEY `ix_sending_job_details_message_id` (`message_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `sending_jobs` (
  `id` int NOT NULL AUTO_INCREMENT,
  `user_id` int DEFAULT NULL,
  `batch_id` varchar(64) DEFAULT NULL,
  `template_name` varchar(255) DEFAULT NULL,
  `group_name` varchar(255) DEFAULT NULL,
  `source_email` varchar(255) DEFAULT NULL,
  `total_contacts` int DEFAULT NULL,
  `total_batches` int DEFAULT NULL,
  `status` varchar(50) DEFAULT NULL,
  `error_message` text,
  `configuration_set` varchar(255) DEFAULT NULL,
  `created_at` datetime DEFAULT NULL,
  `sent_count` int DEFAULT '0',
  `finished_at` datetime DEFAULT NULL,
  `reply_to` varchar(255) DEFAULT NULL,
  `template_id` int DEFAULT NULL,
  `group_id` int DEFAULT NULL,
  `from_name` varchar(255) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `ix_sending_jobs_batch_id` (`batch_id`),
  KEY `ix_sending_jobs_id` (`id`),
  KEY `ix_sending_jobs_user_id` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `system_settings` (
  `id` int NOT NULL AUTO_INCREMENT,
  `key` varchar(128) NOT NULL,
  `value` text,
  `updated_at` datetime DEFAULT (now()),
  PRIMARY KEY (`id`),
  UNIQUE KEY `ix_system_settings_key` (`key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `template_attachments` (
  `id` int NOT NULL AUTO_INCREMENT,
  `template_id` int NOT NULL,
  `file_name` varchar(255) NOT NULL,
  `file_path` varchar(500) NOT NULL,
  `content_type` varchar(128) NOT NULL,
  `file_size` int DEFAULT '0',
  `created_at` datetime DEFAULT (now()),
  PRIMARY KEY (`id`),
  KEY `ix_template_attachments_template_id` (`template_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `unsubscribe_list` (
  `id` int NOT NULL AUTO_INCREMENT,
  `email` varchar(255) NOT NULL,
  `source_email` varchar(255) NOT NULL,
  `reason` varchar(32) DEFAULT 'one-click',
  `unsubscribed_at` datetime DEFAULT (now()),
  PRIMARY KEY (`id`),
  UNIQUE KEY `ix_unsub_email_source` (`email`,`source_email`),
  KEY `ix_unsubscribe_list_source_email` (`source_email`),
  KEY `ix_unsubscribe_list_email` (`email`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;
/*!40101 SET @saved_cs_client     = @@character_set_client */;
/*!50503 SET character_set_client = utf8mb4 */;
CREATE TABLE `users` (
  `id` int NOT NULL AUTO_INCREMENT,
  `username` varchar(100) DEFAULT NULL,
  `display_name` varchar(255) DEFAULT NULL,
  `hashed_password` varchar(255) DEFAULT NULL,
  `email` varchar(255) DEFAULT NULL,
  `is_admin` tinyint(1) DEFAULT NULL,
  `is_active` tinyint(1) DEFAULT NULL,
  `created_at` datetime DEFAULT NULL,
  `daily_send_limit` int DEFAULT '1000',
  `unsub_config` text,
  `contact_email` varchar(255) DEFAULT NULL,
  `sender_name` varchar(255) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `ix_users_username` (`username`),
  KEY `ix_users_id` (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
/*!40101 SET character_set_client = @saved_cs_client */;

-- +goose StatementEnd

-- +goose Down
-- 基线不可整体回退（上游无更早状态）
