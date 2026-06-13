-- Enforce globally unique organization names.
CREATE UNIQUE INDEX "organizations_name_key" ON "organizations"("name");

-- Enforce at most one organization membership per user (one org per user, total).
-- Replaces the previous non-unique index on user_id.
DROP INDEX "organization_members_user_id_idx";
CREATE UNIQUE INDEX "organization_members_user_id_key" ON "organization_members"("user_id");
