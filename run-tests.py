from itertools import product
import subprocess as sp
import tempfile
import atexit
import shutil
import sys
import os

def shell(cmd):
    return sp.check_output(cmd.split()).rstrip()

symlink = lambda target: lambda name: os.symlink(target, name)
contains = lambda data: lambda name: open(name, "w").write(data)
mkdir = os.mkdir
empty = lambda name: open(name, "w")
nothing = lambda name: None
size = lambda n: contains("x" * n)

tests = [
    'mkdir',
    'empty',
    'nothing',
    'symlink("a")',
    'symlink("b")',
    'contains("foo")',
    'contains("bar")',
    'size(1)',
    'size(2)',
]

tempdir = tempfile.mkdtemp()
atexit.register(lambda: shutil.rmtree(tempdir))
shell("go build") 
shell("mv backup-chk %s/" %(tempdir, ))
os.chdir(tempdir)

for test in tests:
    os.mkdir(test)
    eval(test + "(%r)" %("%s/testfile" %(test, ), ), globals(), locals())

test_count = 0
err_count = 0
for a, b in product(tests, tests):
    test_count += 1
    res = shell("./backup-chk %s:%s" %(a, b))
    if a == b and res:
        print "ERROR: Output when both sides are identical: %s: %s" %(a, res)
        err_count += 1
    elif a != b and not res and a != "nothing":
        print "ERROR: No output found when it was expected: %s <-> %s" %(a, b)
        err_count += 1

print "%s tests, %s failures" %(test_count, err_count)
