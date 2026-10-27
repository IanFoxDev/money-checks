-- Transactions whose entries do not sum to zero in a currency: a fee booked with
-- a different amount on each side, or a conversion that lost a leg when the
-- process died between two inserts outside one database transaction.
-- The window picks the transactions with an entry since @since and sums all their
-- entries, so a transaction that started before the window is not cut in half.
select concat(e.txn_id, ':', a.currency) as id, a.currency,
       sum(e.amount) as imbalance, count(*) as entries
from entries e
join accounts a on a.id = e.account_id
where e.txn_id in (select txn_id from entries where created_at >= @since)
group by e.txn_id, a.currency
having sum(e.amount) <> 0
order by 1
