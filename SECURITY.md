# Security

## Reporting a problem

Please do not open a public issue for something that could put members at risk. Use
GitHub's **Report a vulnerability** button on this repository (Security tab), which opens a
private conversation with the maintainers.

Worth reporting, for example:

- a way to make aNLpt send a file that is not in a shared folder;
- a way for a web page or another program to use aNLpt's local page;
- a way to learn who is on the other side of a transfer;
- a way to get the sign-in tokens out of the program.

## What aNLpt assumes

- The archive it talks to is the one in its configuration, over HTTPS. It is not trusted
  with anything beyond what is listed in the README: it can ask for files that were
  shared, by id, and nothing else.
- Other programs running as the same user on the same computer are trusted, as they are by
  every desktop program: they could read the shared folders themselves.

## Releases

Releases are built by GitHub Actions from a tagged commit, without a packer, an obfuscator
or anything else that would make the file differ from what the source produces. Each
release carries a `SHA256SUMS` file. The Windows build is not code signed.
