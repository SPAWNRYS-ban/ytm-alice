FROM debian:buster-slim
RUN printf 'deb http://archive.debian.org/debian buster main\ndeb http://archive.debian.org/debian-security buster/updates main\n' > /etc/apt/sources.list \
 && dpkg --add-architecture armhf \
 && apt-get -o Acquire::Check-Valid-Until=false update \
 && apt-get install -y --no-install-recommends gcc-arm-linux-gnueabihf libc6-dev-armhf-cross libgstreamer1.0-dev:armhf libglib2.0-dev:armhf pkg-config python3
ENV PKG_CONFIG_LIBDIR=/usr/lib/arm-linux-gnueabihf/pkgconfig:/usr/share/pkgconfig
WORKDIR /src
COPY player.c link-stubs.py ./
# Public 1.x headers supply the ABI; firmware libraries stay on the device.
RUN arm-linux-gnueabihf-gcc -Os -Wall -Wextra -Werror -c player.c -o player.o $(pkg-config --cflags gstreamer-1.0) \
 && python3 link-stubs.py \
 && arm-linux-gnueabihf-gcc player.o -L. -lquasar_gstreamer -lglib -s -o /native-player \
 && arm-linux-gnueabihf-readelf -d /native-player
