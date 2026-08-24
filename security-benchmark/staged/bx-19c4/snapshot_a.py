def execute(data):
    released = data["amount"] if data["sender_ok"] else 0
    return {"released": released}
