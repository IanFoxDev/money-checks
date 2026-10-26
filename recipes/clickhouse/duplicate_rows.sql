-- Payments that are in the analytics table more than once: a consumer crashed
-- after the insert and before it committed its offset, so the batch came again.
-- Every sum over the table is now too large, and the dashboards and partner
-- reports built on it with it. A payment and its refund are not duplicates.
select payment_id || ':' || kind as id, any(currency) as currency,
       count() as copies, sum(amount) - any(amount) as extra
from payments
where created_at >= {since:DateTime64(6, 'UTC')}
group by payment_id, kind
having count() > 1
order by id
