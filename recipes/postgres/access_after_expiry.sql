-- Subscriptions still active 3 days after their period ended: the renewal
-- failed or was never tried, and the job that expires them did not run or
-- skipped them. The customer keeps access without paying, every period.
-- 3 days is the grace period, while renewals are retried; use yours.
select id, owner, price, currency, current_period_end
from subscriptions
where status = 'active' and current_period_end < now() - interval '3 days'
order by id
