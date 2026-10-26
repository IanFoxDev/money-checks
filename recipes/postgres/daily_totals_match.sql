-- The app side of daily_totals_match: successful payments per day and
-- currency. $1 is the start of the window, $2 the end that settle gives both
-- sides, so a payment still on its way to analytics is not a difference.
select to_char(created_at at time zone 'UTC', 'YYYY-MM-DD') as day, currency,
       sum(amount) as amount, count(*) as payments
from payments
where status = 'succeeded' and created_at >= $1 and created_at < $2
group by 1, 2
