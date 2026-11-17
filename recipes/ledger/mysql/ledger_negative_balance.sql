-- Accounts below zero that were not opened with allowNegative: the last posting
-- holds the balance. The library checks this under the account's lock, so a row
-- here was written past it.
select a.code as id, a.currency, p.balance_after as balance
from ledger_accounts a
join (select account, max(sequence) as sequence from ledger_postings group by account) last
  on last.account = a.code
join ledger_postings p on p.account = last.account and p.sequence = last.sequence
where a.allow_negative = 0 and p.balance_after < 0
order by a.code
