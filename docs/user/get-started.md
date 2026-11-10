---
{"id":"get-started","title":"Get started","description":"The agent keeps the books. You review them in your browser. Start on Vult Cloud or run Vult yourself.","group":"Start here","order":1,"keywords":["start","setup","install","cloud","sign up","book","create","macos","ubuntu"]}
---

## How Vult works {#how-vult-works}

Vult is a double-entry ledger built for agents. The agent reads receipts, invoices and statements, then records balanced debits and credits in your Book. You open the same Book in your browser to see what was recorded, check the numbers and approve the entries the agent asks about.

A **Book** is one set of accounts in one currency, such as one company or your personal finances. Every entry in a Book balances, and posted entries are never edited in place. A correction is a new, linked reversal, so the history stays readable.

There are two ways to run Vult. Both use the same Book, browser and agent tools.

- **Vult Cloud:** we run Vult for you. Sign up on vult.ai and connect an agent with a web address. Nothing to install.
- **Vult on your own computer:** install the open-source app on a Mac or an Ubuntu computer. Your Books stay on that machine.

## Start on Vult Cloud {#cloud}

1. Go to [vult.ai/signup](https://vult.ai/signup) and choose monthly or yearly billing.
2. Sign in with an emailed code or with GitHub.
3. Set up an authenticator app. Vult Cloud asks for it every time you sign in. Save the recovery codes it shows you somewhere safe and offline.
4. Pay, then choose the address for your Vult.
5. Choose **Open Vult** to see your Book.

Next, [connect an agent](./connect-an-agent.md#cloud) so it can start recording.

## Install Vult on your computer {#install}

Vult runs on **macOS 26 on Apple Silicon** and **Ubuntu 24.04 LTS on x86-64**. The installer includes everything Vult needs, including its own database. You don't need Docker.

On a Mac, run this from your own terminal as your normal user, not with sudo:

```sh
curl -fsSL https://vult.sh | sh
```

The script checks your Mac, downloads one fixed release, verifies its checksum and asks for administrator approval to install. To read it first, download it with `curl -fsSL https://vult.sh -o install.sh`, then run `sh install.sh`. For Ubuntu, use the native Ubuntu installer published with each release in the [Vult repository](https://github.com/vult-ai/vult).

Then set up the ledger and create your first Book:

```sh
vult init
vult book create --name "Personal" --currency USD --scale 2
vult book list
```

`vult init` shows a **recovery key** once. It unlocks your encrypted backups. Store it offline, away from your agents. On a Mac, keep FileVault turned on.

The currency and scale are fixed when the Book is created. A scale of 2 means amounts are kept in cents. Create a separate Book for each business or person.

## Open your Book in the browser {#open-in-browser}

Run this from your own terminal:

```sh
vult open
```

The first time, your Mac asks you to trust Vult's local web address. Vult then asks for administrator approval and sets up a passkey for the browser. Save the passkey and keep the recovery codes it shows you offline. If you lose the passkey, `vult browser reset` replaces it without touching your Books.

Next, [connect an agent](./connect-an-agent.md#on-your-computer).

## What Vult does and doesn't do {#scope}

Vult records financial events that have already happened, keeps the original documents beside the entries, and produces your statements. It does not connect to your bank, move money, pay bills or file taxes. The agent decides what to record. Vult checks that every entry balances, keeps every change on the record and leaves the final say on uncertain entries to you.
