from fabric.api import run, env, cd


env.user = 'ubuntu'
env.tsuru_path = '/home/ubuntu/.go/src/github.com/timeredbull/tsuru'
env.collector_path = '%s/collector' % env.tsuru_path
env.webserverd_path = '%s/api/webserverd' % env.tsuru_path


def stop():
    run('killall -9 webserverd')
    run('killall -9 collector')


def update():
    run('go get -u github.com/timeredbull/tsuru/collector')
    run('go get -u github.com/timeredbull/tsuru/api/webserverd')


def build():
    with cd(env.collector_path):
        run("go build -o collector main.go")

    with cd(env.webserverd_path):
        run("go build -o webserverd main.go")


def start():
    run("nohup %s/collector >& /dev/null < /dev/null &" % env.collector_path, pty=False)
    run("nohup %s/webserverd >& /dev/null < /dev/null &" % env.webserverd_path, pty=False)


def deploy():
    stop()
    update()
    build()
    start()
