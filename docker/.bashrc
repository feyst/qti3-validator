# The shell of the host user in the dev container.
[ -f /etc/bash.bashrc ] && . /etc/bash.bashrc

export PATH="/home/host/.local/bin:/home/host/go/bin:/usr/local/go/bin:$PATH"
export HISTFILE=/workspace/docker/.bash_history

cd /workspace 2>/dev/null || true

alias ll='ls -la'
PS1='\[\e[32m\]\u@qti3-dev\[\e[0m\]:\[\e[34m\]\w\[\e[0m\]\$ '
