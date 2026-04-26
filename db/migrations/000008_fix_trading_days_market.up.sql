-- market_holidays does not exist as a real table until a subscription of
-- that data type is created, so define a placeholder view with the columns
-- the function references, redefine trading_days, then drop the placeholder.
CREATE VIEW market_holidays AS
  SELECT generate_series(date'2024-01-01', date'2024-01-01', interval '1 day') AS event_date,
         'NYSE'::text AS market,
         false AS early_close
  WHERE false;

CREATE OR REPLACE FUNCTION trading_days(DATE, DATE)
RETURNS SETOF DATE
LANGUAGE SQL
AS $func$
  SELECT dt::date FROM generate_series($1, $2, interval '1' day) as t(dt)
  WHERE extract(dow FROM dt) BETWEEN 1 AND 5
    AND dt NOT IN (SELECT event_date FROM market_holidays WHERE market = 'us' AND early_close = false)
$func$;

DROP VIEW market_holidays;
