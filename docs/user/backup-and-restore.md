---
{"id":"backup-and-restore","title":"Back up, export and restore","description":"Download your Book at any time, keep encrypted backups and restore them safely.","group":"Use your Book","order":6,"keywords":["backup","restore","export","download","csv","json","archive","recovery key","upgrade","uninstall"]}
---

## Download your data {#download}

Your data is never locked in. In **Settings → Your data**:

- **Whole Book:** every entry and account, with the original documents, as one archive.
- **Entries and accounts:** CSV files for spreadsheets and other software.

Every report in **Statements** also downloads as CSV. These downloads work on Vult Cloud and on your own computer.

Downloads are not encrypted. Store them somewhere private.

## Back up Vult on your computer {#create-a-backup}

Run this from your own terminal:

```sh
vult backup --output books.age
```

A backup holds every Book in the installation, including entries, originals, approvals and their history. It is encrypted with the recovery key that `vult init` showed you. Keep dated copies, including one away from this computer, and keep the recovery key separately.

You can also export one Book:

```sh
vult export --book BOOK_ID --output journal.json
vult export --documents --book BOOK_ID --output book.tar
```

Exports are plain, unencrypted files. Vult never overwrites an existing file.

## Restore a backup {#restore-a-backup}

```sh
vult restore --input books.age
```

Restore needs an empty installation. It brings back every recorded entry, original and approval. It doesn't bring back agent connections; create new ones afterwards.

## Before an upgrade {#before-an-upgrade}

Make a backup before installing a new version. Upgrades keep your Books, entries, originals and connections, and stop rather than start an empty ledger if anything doesn't check out.

## Remove Vult {#uninstall}

```sh
vult uninstall
```

Uninstalling removes the program only. Your Books, credentials and recovery key stay on the computer, and reinstalling picks them up again. Back up first anyway.
