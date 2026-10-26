-- The analytics side of daily_totals_match: successful payments per day and
-- currency, the same rows the app's database counts on the other side.
select toString(toDate(created_at)) as day, currency,
       sum(amount) as amount, count() as payments
from payments
where kind = 'payment'
  and created_at >= {since:DateTime64(6, 'UTC')}
  and created_at < {until:DateTime64(6, 'UTC')}
group by day, currency
