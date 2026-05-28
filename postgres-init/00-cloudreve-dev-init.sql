CREATE DATABASE authverse OWNER cloudreve;

\connect cloudreve
CREATE EXTENSION IF NOT EXISTS pg_trgm;

\connect authverse
CREATE EXTENSION IF NOT EXISTS pg_trgm;
