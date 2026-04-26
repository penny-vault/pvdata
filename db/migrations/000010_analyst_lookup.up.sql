CREATE TABLE analyst_lookup (
    id      SMALLSERIAL PRIMARY KEY,
    analyst TEXT        NOT NULL UNIQUE
);

INSERT INTO analyst_lookup (analyst) VALUES
    ('zacks-rank'),
    ('zacks-value'),
    ('zacks-growth'),
    ('zacks-momentum'),
    ('zacks-vgm');
