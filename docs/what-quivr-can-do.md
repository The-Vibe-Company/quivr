# What Quivr can do

Quivr is an engine that collects content as it is published, makes it searchable within
seconds, and tells you when something new matches a topic you follow. It has no screens of
its own beyond a small demo: other applications use it through its API.

This page is for readers who want to know what it does without reading code. Quivr is at
the evaluation stage: try it, but do not run it in production yet.

## Collect content as it arrives

- **Send it content**, one article at a time or in batches, as text or as files such as PDFs.
- **Let it fetch content by itself** from news feeds (RSS or Atom), Microsoft 365
  mailboxes or X lists, on a schedule you choose.
- **Correct or withdraw** an article later. Quivr keeps every earlier version, so you can
  see what was published and when.
- **Nothing is lost.** Once Quivr confirms it has received something, it is stored safely,
  even if a part of the system is down for a while. Sending the same thing twice does not
  create a duplicate.

## Find it again

- **Search by words or by meaning.** A search for "flooding" can also find an article about
  "rivers bursting their banks".
- **See where each result comes from**: which article, which version and which part of it.
- **Search only what you are allowed to see.** Content is grouped into collections, and
  each access key is limited to the collections and actions it was given.

## Follow a topic

- **Save a search as an alert**, such as `"electric cars" AND (battery OR charging) NOT review`.
- **Or describe the topic in a sentence**, such as "new rules on charging electric cars". An
  outside classifier, when turned on, then also catches articles that use other words or
  another language.
- **Get notified** each time a new article matches, at a web address set up by whoever runs
  Quivr, saying why it matched.
- **Hear about changes**: when a matching article is corrected, or withdrawn, the alert says so.
- **Set up alerts for your own users**: an application can create alerts on behalf of each
  of its users and route every notification to the right person.

## Adapt it to your field

Quivr keeps its core generic. Support for a new file format or a new kind of alert rule
is added as a **plugin**, without changing Quivr itself. Plugins can be written in Python,
and Quivr includes a tool that checks a plugin works before you use it.

## What it does not do yet

Quivr does not read scanned documents (images of text). Alerts described in a sentence
need an outside classifier service: Quivr cannot yet decide them on its own. The
[remaining limits](quivr-v2-remaining-limits.md) page lists what is known not to work.

## Where to go next

- [Using Quivr](start/functional.md): guides to run Quivr and try it.
- [Writing plugins](start/plugin-author.md): extend Quivr for your own content.
- [Contributing to Quivr](start/contributor.md): change Quivr itself.
