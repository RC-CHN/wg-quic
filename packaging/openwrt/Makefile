include $(TOPDIR)/rules.mk

WG_QUIC_VERSION ?= $(strip $(shell sed -n '1p' "$(CURDIR)/VERSION" 2>/dev/null))
ifeq ($(WG_QUIC_VERSION),)
$(error WG_QUIC_VERSION is required)
endif

PKG_NAME:=wg-quic
PKG_VERSION:=$(WG_QUIC_VERSION)
PKG_RELEASE:=1
PKG_LICENSE:=GPL-3.0-only
PKG_LICENSE_FILES:=LICENSE
PKG_MAINTAINER:=RC-CHN <rc-chn@users.noreply.github.com>

PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk

define Package/wg-quic
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=VPN
  TITLE:=WireGuard datagrams over QUIC
  URL:=https://github.com/RC-CHN/wg-quic
  DEPENDS:=+kmod-tun +ip-full
endef

define Package/wg-quic/description
 wg-quic transports encrypted WireGuard datagrams over QUIC. This package
 includes the data plane, the wg-quic-quick host-policy supervisor, and an
 OpenWrt procd multi-instance service.
endef

define Build/Prepare
	@test -x "$(WG_QUIC_BIN_DIR)/wg-quic" || { \
		echo "WG_QUIC_BIN_DIR must contain an executable wg-quic" >&2; \
		exit 1; \
	}
	@test -x "$(WG_QUIC_BIN_DIR)/wg-quic-quick" || { \
		echo "WG_QUIC_BIN_DIR must contain an executable wg-quic-quick" >&2; \
		exit 1; \
	}
	$(INSTALL_DIR) $(PKG_BUILD_DIR)
	$(INSTALL_BIN) $(WG_QUIC_BIN_DIR)/wg-quic $(PKG_BUILD_DIR)/wg-quic
	$(INSTALL_BIN) $(WG_QUIC_BIN_DIR)/wg-quic-quick $(PKG_BUILD_DIR)/wg-quic-quick
endef

define Build/Compile
endef

define Package/wg-quic/conffiles
/etc/config/wg-quic
endef

define Package/wg-quic/install
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/wg-quic $(1)/usr/bin/wg-quic
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/wg-quic-quick $(1)/usr/bin/wg-quic-quick
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/wg-quic.init $(1)/etc/init.d/wg-quic
	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./files/wg-quic.config $(1)/etc/config/wg-quic
	$(INSTALL_DIR) $(1)/etc/wg-quic
	chmod 0700 $(1)/etc/wg-quic
endef

$(eval $(call BuildPackage,wg-quic))
