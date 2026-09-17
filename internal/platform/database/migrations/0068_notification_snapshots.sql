-- 通知是历史事件：列表展示使用创建时快照，不在读取热路径关联用户、影片和资源表。
-- 目标 ID 和外键继续保留，点击时直接跳转，由目标页面负责不存在或未公开状态。
-- 线上迁移只做 PostgreSQL 11+ 的元数据级加列，不回填旧行、不制造全表 UPDATE/WAL。
-- 拿不到表锁时快速失败并回滚，避免迁移排队阻塞正在运行的消息请求。
SET LOCAL lock_timeout = '1s';
SET LOCAL statement_timeout = '5s';

ALTER TABLE social_notifications
    ADD COLUMN actor_name text NOT NULL DEFAULT '',
    ADD COLUMN actor_avatar text NOT NULL DEFAULT '',
    ADD COLUMN movie_title text NOT NULL DEFAULT '',
    ADD COLUMN content text NOT NULL DEFAULT '';

-- 迁移执行器会把后续版本放在同一事务里，恢复默认值，避免影响未来迁移。
SET LOCAL lock_timeout = '0';
SET LOCAL statement_timeout = '0';
