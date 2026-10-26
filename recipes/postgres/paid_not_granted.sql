-- Orders paid more than 30 minutes ago and still not fulfilled: the event that
-- grants access was lost between payments and fulfilment, or fulfilment failed
-- and nothing retried it. The customer paid and got nothing; a support ticket
-- or a chargeback comes next. A payment refunded in full has nothing to fulfil.
select o.id, p.currency, p.amount, p.id as payment_id, p.created_at as paid_at
from orders o
join payments p on p.order_id = o.id
where p.status = 'succeeded'
  and o.fulfilled_at is null
  and p.created_at < now() - interval '30 minutes'
  and p.amount > coalesce((select sum(r.amount) from refunds r where r.payment_id = p.id), 0)
order by o.id
