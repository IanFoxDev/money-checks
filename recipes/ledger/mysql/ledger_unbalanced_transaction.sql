-- Transactions of ianfoxdev/ledger whose debits and credits differ in a currency.
-- The library refuses to write one, so a row here was written around it: a script
-- that inserted postings by hand, a migration, a posting deleted afterwards.
-- imbalance is debits minus credits. The window picks transactions since @since.
select concat(t.id, ':', a.currency) as id, a.currency, t.idempotency_key, t.type,
       sum(case when p.side = 'D' then p.amount else -p.amount end) as imbalance
from ledger_transactions t
join ledger_postings p on p.transaction_id = t.id
join ledger_accounts a on a.code = p.account
where t.created_at >= @since
group by t.id, a.currency, t.idempotency_key, t.type
having sum(case when p.side = 'D' then p.amount else -p.amount end) <> 0
order by t.id
