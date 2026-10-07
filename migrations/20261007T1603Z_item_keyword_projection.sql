-- Existing generations retain their immutable passage keyword index. New and
-- rebuilt generations opt in explicitly at creation, never by retroactive default.
ALTER TABLE projection_generations ADD COLUMN item_keywords_projected boolean NOT NULL DEFAULT false;
