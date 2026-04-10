CREATE OR REPLACE FUNCTION trading_days(DATE, DATE)
RETURNS SETOF DATE
LANGUAGE SQL
AS $func$
  SELECT dt::date FROM generate_series($1, $2, interval '1' day) as t(dt)
  WHERE extract(dow FROM dt) BETWEEN 1 AND 5
    AND dt NOT IN (SELECT event_date FROM market_holidays WHERE market = 'us' AND early_close = false)
$func$;
