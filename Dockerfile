FROM gliderlabs/alpine
MAINTAINER ChannelMeter <product@channelmeter.com>

WORKDIR /opt
ADD rmg /opt/rmg

CMD ["/opt/rmg"]