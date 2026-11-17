-- Accounts whose sequence has a hole: postings 1 to N with fewer than N rows. The
-- library numbers postings without gaps and never deletes one, so a hole is a row
-- someone removed. Readers that page by (account, sequence) skip past it.
select account as id, count(*) as postings, max(sequence) as last_sequence
from ledger_postings
group by account
having count(*) <> max(sequence)
order by account
