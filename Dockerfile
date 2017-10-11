FROM alpine:latest

LABEL Author="Jordan McMichael <jlmcmchl@gmail.com>"

WORKDIR "/opt"

ADD .docker_build/tbc-discord-bot /opt/bin/tbc-discord-bot

CMD ["/opt/bin/tbc-discord-bot"]