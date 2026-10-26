-- One provider transaction recorded as two payments: a webhook delivered twice
-- and handled twice, or a redirect and a webhook racing each other. The customer
-- paid once, the books show two payments. The fix is a unique index on
-- (provider, provider_txn_id); this finds the rows that got in before it.
-- The window picks provider ids seen since $1 and counts all their payments, so a
-- webhook the provider sends again a week later is still caught.
select provider || ':' || provider_txn_id as id, currency,
       count(*) as payments, string_agg(id, ',' order by id) as payment_ids,
       sum(amount) - min(amount) as extra
from payments
where status = 'succeeded'
  and (provider, provider_txn_id) in (
      select provider, provider_txn_id from payments where created_at >= $1)
group by provider, provider_txn_id, currency
having count(*) > 1
order by 1
