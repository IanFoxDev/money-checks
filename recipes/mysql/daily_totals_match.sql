-- The app side of daily_totals_match: successful payments per day and
-- currency. @since is the start of the window, @until the end that settle gives
-- both sides, so a payment still on its way to analytics is not a difference.
-- money-checks runs every query in UTC, so the day is the UTC day, as in
-- ClickHouse.
select date_format(created_at, '%Y-%m-%d') as day, currency,
       sum(amount) as amount, count(*) as payments
from payments
where status = 'succeeded' and created_at >= @since and created_at < @until
group by 1, 2
