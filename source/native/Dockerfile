FROM debian:buster-slim
RUN printf 'deb http://archive.debian.org/debian buster main\ndeb http://archive.debian.org/debian-security buster/updates main\n' > /etc/apt/sources.list \
 && dpkg --add-architecture armhf \
 && dpkg --add-architecture arm64 \
 && apt-get -o Acquire::Check-Valid-Until=false update \
 && apt-get install -y --no-install-recommends gcc-arm-linux-gnueabihf libc6-dev-armhf-cross gcc-aarch64-linux-gnu libc6-dev-arm64-cross libgstreamer1.0-dev:armhf libglib2.0-dev:armhf libgstreamer1.0-dev:arm64 libglib2.0-dev:arm64 pkg-config python3
ENV PKG_CONFIG_LIBDIR=/usr/lib/arm-linux-gnueabihf/pkgconfig:/usr/share/pkgconfig
WORKDIR /src
COPY player.c link-stubs.py resolver.c ./
COPY .build/quickjs-amalgam.c .build/quickjs.h ./
RUN arm-linux-gnueabihf-gcc -O2 -static -D_GNU_SOURCE resolver.c quickjs-amalgam.c -lm -ldl -pthread -latomic -s -o /native-resolver
# Public 1.x headers supply the ABI; firmware libraries stay on the device.
RUN arm-linux-gnueabihf-gcc -Os -Wall -Wextra -Werror -c player.c -o player.o $(pkg-config --cflags gstreamer-1.0) \
 && CC=arm-linux-gnueabihf-gcc NM=arm-linux-gnueabihf-nm python3 link-stubs.py \
 && arm-linux-gnueabihf-gcc player.o -L. -lquasar_gstreamer -lglib -s -o /native-player \
 && arm-linux-gnueabihf-readelf -d /native-player

# ARM64-only userspaces (Mini 3 Pro, Station 3). Use the same public ABI.
RUN aarch64-linux-gnu-gcc -Os -Wall -Wextra -Werror -c player.c -o player.o $(PKG_CONFIG_LIBDIR=/usr/lib/aarch64-linux-gnu/pkgconfig:/usr/share/pkgconfig pkg-config --cflags gstreamer-1.0) \
 && CC=aarch64-linux-gnu-gcc NM=aarch64-linux-gnu-nm python3 link-stubs.py \
 && aarch64-linux-gnu-gcc player.o -L. -lquasar_gstreamer -lglib -s -o /native-player-arm64 \
 && aarch64-linux-gnu-readelf -d /native-player-arm64
