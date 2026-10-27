-- Payments refunded for more than they were paid: a refund retried after a
-- timeout when the first one went through, or a manual refund in the provider's
-- dashboard on top of the automatic one. Partial refunds that add up to the
-- payment are fine. The window picks payments with a refund since @since and sums
-- all their refunds.
select p.id, p.currency, p.amount, sum(r.amount) as refunded, count(*) as refunds,
       sum(r.amount) - p.amount as `over`  -- over is a reserved word in MySQL 8
from payments p
join refunds r on r.payment_id = p.id
where p.id in (select payment_id from refunds where created_at >= @since)
group by p.id
having sum(r.amount) > p.amount
order by p.id
