backup-chk
==========

**!! WARNING !!** VERY ALPHA SOFTWARE **!! WARNING !!**

``backup-chk`` probably won't trash your hard drive, steal your secrets, or
send passive-aggressive notes to your noisy neighbours... but it's definitely
got some issues that need work (see ``notes.txt``).

**!! WARNING !!** VERY ALPHA SOFTWARE **!! WARNING !!**

Are your backups actually zeros?

``backup-chk`` will help you figure out!

::

    $ backup-chk
    Usage:
      backup-chk [OPTIONS] [-vv] [--time-machine] [REFERENCE_DIR:BACKUP_DIR ...]

    Application Options:
      -v, --verbose       Show verbose debug information
      -t, --time-machine  Use Time Machine defaults
      -x, --exclude=      Exclude files with relative paths matching this pattern. Matching is simple glob matching (ex,
                          'foo*bar' matches 'foo/x/bar', 'foobar', and 'foo-bar')
      -c, --config-dir=   Configuration and status directory (default: ~/.backup-chk/)

    Help Options:
      -h, --help          Show this help message

    Example:
      $ .../exe/backup-chk --time-machine
      $ .../exe/backup-chk /Users/wolever:/Volumes/Backup/Users/wolever

    $ backup-chk -v --time-machine
    2017-02-20 15:37:25.085 INFO Checking: '/Volumes/Backup/Backups.backupdb/Latest/Macintosh HD/Users' against '/Users'
    2017-02-20 15:37:25.085 INFO Loading previous run state from cache...
    2017-02-20 15:37:29.206 WARNING wolever/some-file: size do not match: reference 4 != backup 7
    42,437 checked / 0 errors @ 108.93GB/s (...arch-test/lib/python2.7/site-packages/pip-1.1-py2.7.egg/pip/status_codes.py)

Installation
------------

Either install with go::

    $ go get github.com/wolever/backup-chk

Or download a pre-built OS X binary: https://github.com/wolever/backup-chk/raw/binaries/backup-chk
