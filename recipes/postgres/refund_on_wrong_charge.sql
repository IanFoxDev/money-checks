-- Refunds written against another charge than the one the provider refunded.
-- A subscription renewal is refunded at the provider, the webhook names that
-- charge, and the app looks the payment up by subscription and takes the first
-- one. The refunded renewal still counts as paid in revenue and partner
-- shares, and the first charge looks refunded. refund_exceeds_payment notices
-- only when the first charge collects more refunds than it was worth.
-- refunded_payment is the payment the provider meant, when the app has it.
select r.id, p.currency, r.amount, r.payment_id,
       p.provider_txn_id as linked_txn, r.provider_txn_id as refunded_txn,
       (select min(q.id) from payments q
         where q.provider = p.provider and q.provider_txn_id = r.provider_txn_id) as refunded_payment
from refunds r
join payments p on p.id = r.payment_id
where r.created_at >= $1
  and r.provider_txn_id is not null
  and r.provider_txn_id <> p.provider_txn_id
order by r.id
