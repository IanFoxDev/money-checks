-- Accounts whose stored balance is not the sum of their entries: the balance
-- updated twice when a request was retried, or changed by hand in a support tool
-- without an entry. One statement reads one snapshot, so a payment written in one
-- transaction with its entries never shows up half done.
-- It reads every account, so the check runs every few hours, not every interval.
select a.id, a.owner, a.currency, a.balance,
       coalesce(sum(e.amount), 0) as entries_sum,
       a.balance - coalesce(sum(e.amount), 0) as difference
from accounts a
left join entries e on e.account_id = a.id
group by a.id
having a.balance <> coalesce(sum(e.amount), 0)
order by a.id
