-- Credit lots past their expiry for more than an hour that still hold credits: the
-- cron with "ledger credits:expire" did not run, or stopped at --limit. The customer
-- can still spend them, and breakage is not booked. The expiry is in the lot's code,
-- <account>@lot:YYYYMMDDTHHMMSSZ:<12 hex>, and compares as text.
select a.code as id, a.currency, p.balance_after as balance,
       substring(a.code from position('@lot:' in a.code) + 5 for 16) as expires
from ledger_accounts a
join (select account, max(sequence) as sequence from ledger_postings group by account) last
  on last.account = a.code
join ledger_postings p on p.account = last.account and p.sequence = last.sequence
where a.code like '%@lot:%'
  and substring(a.code from position('@lot:' in a.code) + 5 for 16)
      < to_char(now() at time zone 'utc' - interval '1 hour', 'YYYYMMDD"T"HH24MISS"Z"')
  and p.balance_after > 0
order by a.code
