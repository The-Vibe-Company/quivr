-- Archive changes visibility without removing data.
ALTER TABLE corpora ADD COLUMN archived boolean NOT NULL DEFAULT false;
