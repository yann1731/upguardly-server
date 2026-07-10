-- AlterEnum: TELEGRAM alert channel.
-- Kept alone in its own migration: `migrate deploy` wraps each file in a
-- transaction, and Postgres forbids *using* a value added by
-- ALTER TYPE ... ADD VALUE inside the same transaction that added it.
ALTER TYPE "AlertChannel" ADD VALUE 'TELEGRAM';
