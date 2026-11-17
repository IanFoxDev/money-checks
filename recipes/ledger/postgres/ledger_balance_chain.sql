-- Postings whose balance_after is not the balance before plus the posting: a row
-- "fixed" by hand, or a posting deleted under it. difference is what the stored
-- balance has on top of what the postings add up to. The balance is on the
-- account's normal side: debits add on asset and expense accounts, credits on the
-- others.
select account || '#' || sequence as id, currency, idempotency_key,
       balance_after - expected as difference
from (
    select p.account, p.sequence, p.balance_after, a.currency, t.idempotency_key,
           coalesce(lag(p.balance_after) over (partition by p.account order by p.sequence), 0)
           + case when (p.side = 'D') = (a.type in ('asset', 'expense')) then p.amount else -p.amount end
           as expected
    from ledger_postings p
    join ledger_accounts a on a.code = p.account
    join ledger_transactions t on t.id = p.transaction_id
) chain
where balance_after <> expected
order by account, sequence
