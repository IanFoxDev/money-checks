-- One provider transaction recorded as two payments: a webhook delivered twice
-- and handled twice, or a redirect and a webhook racing each other. The customer
-- paid once, the books show two payments. The fix is a unique index on
-- (provider, provider_txn_id); this finds the rows that got in before it.
-- The window picks provider ids seen since @since and counts all their payments, so a
-- webhook the provider sends again a week later is still caught.
select concat(provider, ':', provider_txn_id) as id, currency,
       count(*) as payments, group_concat(id order by id separator ',') as payment_ids,
       sum(amount) - min(amount) as extra
from payments
where status = 'succeeded'
  and (provider, provider_txn_id) in (
      select provider, provider_txn_id from payments where created_at >= @since)
group by provider, provider_txn_id, currency
having count(*) > 1
order by 1
